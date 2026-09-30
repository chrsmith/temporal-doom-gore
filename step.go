package gore

// Step API: drive the engine one tic at a time from a deterministic host.
//
// The regular Run() loop is clock driven: it reads the wall clock to decide
// how many tics to run, and builds ticcmds from keyboard/mouse events. The
// step API removes both of those. The host supplies every ticcmd explicitly,
// and game time is derived purely from the number of tics that have been run.
// Given the same WAD, the same arguments and the same sequence of ticcmds,
// the engine always ends up in exactly the same state. This is the same
// property that makes vanilla .lmp demos work.

import (
	"bytes"
	"errors"
	"hash/fnv"
	"image"
	"io"
	"io/fs"
	"time"
)

// dg_step_mode is set when the engine is driven through the step API.
var dg_step_mode bool

// Button bits for TicCmd.Buttons (see d_event.h).
const (
	ButtonAttack      = 1
	ButtonUse         = 2
	ButtonChange      = 4    // change weapon; weapon number in ButtonWeaponMask
	ButtonWeaponMask  = 0x38 // (weapon number) << ButtonWeaponShift
	ButtonWeaponShift = 3
	ButtonSpecial     = 0x80 // pause/savegame; never valid from a remote player
)

// TicCmd is one tic worth of player input; it mirrors vanilla ticcmd_t.
type TicCmd struct {
	Forward   int8  `json:"f,omitempty"` // forward (+) / backward (-) movement
	Side      int8  `json:"s,omitempty"` // strafe right (+) / left (-)
	AngleTurn int16 `json:"t,omitempty"` // turn; <<16 is added to the view angle
	Buttons   uint8 `json:"b,omitempty"`
}

// Stats is a snapshot of interesting game state, cheap to compute every tic.
type Stats struct {
	Tic          int32  `json:"tic"`
	LevelTime    int32  `json:"levelTime"`
	GameState    string `json:"gameState"`
	Skill        int32  `json:"skill"`
	Episode      int32  `json:"episode"`
	Map          int32  `json:"map"`
	Health       int32  `json:"health"`
	Armor        int32  `json:"armor"`
	Weapon       int32  `json:"weapon"`
	Ammo         int32  `json:"ammo"` // ammo for the ready weapon, -1 if it needs none
	Dead         bool   `json:"dead"`
	Kills        int32  `json:"kills"`
	TotalKills   int32  `json:"totalKills"`
	Items        int32  `json:"items"`
	TotalItems   int32  `json:"totalItems"`
	Secrets      int32  `json:"secrets"`
	TotalSecrets int32  `json:"totalSecrets"`
	Checksum     uint32 `json:"checksum"`
}

type nullFrontend struct{}

func (nullFrontend) DrawFrame(img *image.RGBA)                       {}
func (nullFrontend) SetTitle(title string)                           {}
func (nullFrontend) GetEvent(event *DoomEvent) bool                  { return false }
func (nullFrontend) CacheSound(name string, data []byte)             {}
func (nullFrontend) PlaySound(name string, channel, volume, sep int) {}

// StepInit initialises the engine for stepping. args are regular DOOM command
// line arguments, e.g. {"-iwad", "doom1.wad", "-warp", "1", "1", "-skill", "3"}.
// Only one engine may exist per process (or per WASM instance).
func StepInit(fe DoomFrontend, args []string) {
	if fe == nil {
		fe = nullFrontend{}
	}
	dg_frontend = fe
	dg_step_mode = true
	dg_exiting = false
	start_time = time.Time{}
	myargs = append([]string{"doom"}, args...)
	m_FindResponseFile()
	DG_ScreenBuffer = image.NewRGBA(image.Rect(0, 0, SCREENWIDTH, SCREENHEIGHT))
	sound_module = &stepSoundModule // see step_sound.go
	d_DoomMain()                    // returns without looping in step mode
}

// StepTic runs exactly one game tic, with cmd as the console player's input.
func StepTic(cmd TicCmd) {
	var cmds [NET_MAXPLAYERS]ticcmd_t
	var ingame [NET_MAXPLAYERS]boolean
	cmds[consoleplayer] = ticcmd_t{
		Fforwardmove: cmd.Forward,
		Fsidemove:    cmd.Side,
		Fangleturn:   cmd.AngleTurn,
		Fbuttons:     cmd.Buttons,
	}
	ingame[consoleplayer] = 1
	stepSoundTic = gametic + 1
	runTic(cmds[:], ingame[:])
	gametic++
	stepUpdateSounds()
}

// StepRender draws the current game state into the frame buffer. It only
// touches renderer state, never game state, so a host can render as often or
// as rarely as it likes without affecting determinism.
func StepRender() {
	d_Display()
}

// FrameBuffer returns the 320x200 palette-indexed frame buffer as last drawn
// by StepRender. The slice is owned by the engine.
func FrameBuffer() []byte {
	return I_VideoBuffer
}

// Palette returns the current 256 entry RGB palette (768 bytes). DOOM uses
// palette swaps for damage/pickup flashes, so this changes over time.
func Palette() []byte {
	pal := make([]byte, 768)
	for i := range 256 {
		pal[i*3] = colors[i].R
		pal[i*3+1] = colors[i].G
		pal[i*3+2] = colors[i].B
	}
	return pal
}

// GetStats returns a snapshot of the current game state.
func GetStats() Stats {
	p := &players[consoleplayer]
	s := Stats{
		Tic:          gametic,
		LevelTime:    leveltime,
		Skill:        int32(gameskill),
		Episode:      gameepisode,
		Map:          gamemap,
		Health:       p.Fhealth,
		Armor:        p.Farmorpoints,
		Weapon:       int32(p.Freadyweapon),
		Ammo:         -1,
		Dead:         p.Fplayerstate == Pst_DEAD,
		Kills:        p.Fkillcount,
		TotalKills:   totalkills,
		Items:        p.Fitemcount,
		TotalItems:   totalitems,
		Secrets:      p.Fsecretcount,
		TotalSecrets: totalsecret,
		Checksum:     Checksum(),
	}
	if int(p.Freadyweapon) < len(weaponinfo) {
		if am := weaponinfo[p.Freadyweapon].Fammo; am != am_noammo {
			s.Ammo = p.Fammo[am]
		}
	}
	switch gamestate {
	case gs_LEVEL:
		s.GameState = "level"
	case gs_INTERMISSION:
		s.GameState = "intermission"
	case gs_FINALE:
		s.GameState = "finale"
	default:
		s.GameState = "demoscreen"
	}
	return s
}

// Checksum hashes the simulation state: RNG index, level time and every map
// object's position, momentum, angle, health and state. Two engines that have
// consumed the same ticcmds from the same starting point return the same
// value; if they don't, they have desynced.
func Checksum() uint32 {
	h := fnv.New32a()
	var buf [4]byte
	put := func(v int32) {
		buf[0], buf[1], buf[2], buf[3] = byte(v), byte(v>>8), byte(v>>16), byte(v>>24)
		h.Write(buf[:])
	}
	put(gametic)
	put(leveltime)
	put(int32(gamestate))
	put(prndindex)
	put(gameepisode)
	put(gamemap)
	if gamestate == gs_LEVEL {
		for th := thinkercap.Fnext; th != nil && th != &thinkercap; th = th.Fnext {
			mo, ok := th.Ffunction.(*mobj_t)
			if !ok {
				continue
			}
			put(mo.Fx)
			put(mo.Fy)
			put(mo.Fz)
			put(int32(mo.Fangle))
			put(mo.Fmomx)
			put(mo.Fmomy)
			put(mo.Fmomz)
			put(int32(mo.Ftype1))
			put(mo.Fhealth)
			put(mo.Ftics)
			put(mo.Fflags)
		}
	}
	p := &players[consoleplayer]
	put(p.Fhealth)
	put(p.Farmorpoints)
	for _, a := range p.Fammo {
		put(a)
	}
	put(int32(p.Freadyweapon))
	return h.Sum32()
}

// CanSave reports whether the game is in a state that SaveGameBytes can
// capture: in a level, with the player alive.
func CanSave() bool {
	return gamestate == gs_LEVEL && players[consoleplayer].Fplayerstate != Pst_DEAD && gameaction == ga_nothing
}

// SaveGameBytes serialises the current level into a vanilla-format savegame,
// followed by a small trailer with state vanilla doesn't save (RNG indices
// and the tic counter). Unlike g_DoSaveGame it does not print "game saved"
// or otherwise touch game state.
func SaveGameBytes() ([]byte, error) {
	if !CanSave() {
		return nil, errors.New("gore: can only save while alive in a level")
	}
	m := &memStream{}
	save_stream = m
	defer func() { save_stream = nil }()
	savegame_error = 0
	p_WriteSaveGameHeader("temporal-doom")
	p_ArchivePlayers()
	p_ArchiveWorld()
	p_ArchiveThinkers()
	p_ArchiveSpecials()
	p_WriteSaveGameEOF()
	saveg_write32(prndindex)
	saveg_write32(rndindex)
	saveg_write32(gametic)
	if savegame_error != 0 {
		return nil, errors.New("gore: error writing savegame")
	}
	return m.buf, nil
}

// LoadGameBytes restores a savegame produced by SaveGameBytes. It should be
// called on a freshly initialised engine.
func LoadGameBytes(data []byte) error {
	m := &memStream{buf: data}
	save_stream = m
	defer func() { save_stream = nil }()
	savegame_error = 0
	if p_ReadSaveGameHeader() == 0 {
		return errors.New("gore: bad savegame header")
	}
	savedleveltime := leveltime
	g_InitNew(gameskill, gameepisode, gamemap)
	leveltime = savedleveltime
	p_UnArchivePlayers()
	p_UnArchiveWorld()
	p_UnArchiveThinkers()
	p_UnArchiveSpecials()
	if p_ReadSaveGameEOF() == 0 {
		return errors.New("gore: bad savegame")
	}
	prndindex = saveg_read32()
	rndindex = saveg_read32()
	gametic = saveg_read32()
	if savegame_error != 0 {
		return errors.New("gore: truncated savegame")
	}
	gameaction = ga_nothing
	return nil
}

// memStream is an in-memory saveStream.
type memStream struct {
	buf []byte
	pos int
}

func (m *memStream) Read(p []byte) (int, error) {
	if m.pos >= len(m.buf) {
		return 0, io.EOF
	}
	n := copy(p, m.buf[m.pos:])
	m.pos += n
	return n, nil
}

func (m *memStream) Write(p []byte) (int, error) {
	if end := m.pos + len(p); end > len(m.buf) {
		m.buf = append(m.buf, make([]byte, end-len(m.buf))...)
	}
	n := copy(m.buf[m.pos:], p)
	m.pos += n
	return n, nil
}

func (m *memStream) Seek(offset int64, whence int) (int64, error) {
	switch whence {
	case io.SeekStart:
		m.pos = int(offset)
	case io.SeekCurrent:
		m.pos += int(offset)
	case io.SeekEnd:
		m.pos = len(m.buf) + int(offset)
	}
	return int64(m.pos), nil
}

func (m *memStream) Close() error { return nil }

// MemFS is a minimal read-only in-memory fs.FS, suitable for handing the
// engine a WAD without giving it any access to a real filesystem.
type MemFS map[string][]byte

func (m MemFS) Open(name string) (fs.File, error) {
	data, ok := m[name]
	if !ok {
		return nil, &fs.PathError{Op: "open", Path: name, Err: fs.ErrNotExist}
	}
	return &memFile{Reader: bytes.NewReader(data), name: name, size: int64(len(data))}, nil
}

type memFile struct {
	*bytes.Reader
	name string
	size int64
}

func (f *memFile) Stat() (fs.FileInfo, error) { return memFileInfo{f.name, f.size}, nil }
func (f *memFile) Close() error               { return nil }

type memFileInfo struct {
	name string
	size int64
}

func (i memFileInfo) Name() string       { return i.name }
func (i memFileInfo) Size() int64        { return i.size }
func (i memFileInfo) Mode() fs.FileMode  { return 0444 }
func (i memFileInfo) ModTime() time.Time { return time.Time{} }
func (i memFileInfo) IsDir() bool        { return false }
func (i memFileInfo) Sys() any           { return nil }
