package gore

import (
	"os"
	"testing"
)

func TestStep(t *testing.T) {
	wadPath := os.Getenv("DOOM_WAD")
	if wadPath == "" {
		wadPath = "doom1.wad"
	}
	wad, err := os.ReadFile(wadPath)
	if err != nil {
		t.Skip("doom1.wad not present")
	}
	SetVirtualFileSystem(MemFS{"doom1.wad": wad})
	StepInit(nil, []string{"-iwad", "doom1.wad", "-warp", "1", "1", "-skill", "3"})
	for i := 0; i < 350; i++ {
		cmd := TicCmd{Forward: 25}
		if i%70 < 20 {
			cmd.AngleTurn = 640
		}
		if i%10 == 0 {
			cmd.Buttons = ButtonAttack
		}
		StepTic(cmd)
	}
	StepRender()
	s := GetStats()
	t.Logf("stats: %+v", s)
	if s.Tic != 350 || s.GameState != "level" {
		t.Fatalf("unexpected stats %+v", s)
	}
	nonzero := 0
	for _, b := range FrameBuffer() {
		if b != 0 {
			nonzero++
		}
	}
	if nonzero < 10000 {
		t.Fatalf("frame looks empty: %d nonzero pixels", nonzero)
	}
	save, err := SaveGameBytes()
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("savegame: %d bytes", len(save))
	before := Checksum()
	if err := LoadGameBytes(save); err != nil {
		t.Fatal(err)
	}
	t.Logf("checksum before save %08x, after load %08x", before, Checksum())
	if GetStats().Tic != 350 {
		t.Fatalf("tic not restored")
	}
}

// TestSaveEveryTic saves after every tic. Removed thinkers linger in the
// thinker list (with a nil function) until the next tic, which used to crash
// p_ArchiveSpecials.
func TestSaveEveryTic(t *testing.T) {
	wadPath := os.Getenv("DOOM_WAD")
	if wadPath == "" {
		wadPath = "doom1.wad"
	}
	wad, err := os.ReadFile(wadPath)
	if err != nil {
		t.Skip("doom1.wad not present")
	}
	SetVirtualFileSystem(MemFS{"doom1.wad": wad})
	StepInit(nil, []string{"-iwad", "doom1.wad", "-warp", "1", "1", "-skill", "3"})
	saves := 0
	for i := 0; i < 3000; i++ {
		cmd := TicCmd{Forward: 0x32}
		if i%140 < 20 {
			cmd.AngleTurn = 1280
		}
		if i%9 == 0 {
			cmd.Buttons |= ButtonAttack
		}
		if i%20 == 0 {
			cmd.Buttons |= ButtonUse
		}
		StepTic(cmd)
		if CanSave() {
			if _, err := SaveGameBytes(); err != nil {
				t.Fatalf("tic %d: %v", i, err)
			}
			saves++
		}
	}
	t.Logf("%d saves, final stats %+v", saves, GetStats())
}

// TestStepSound checks that sounds are reported with the right tic, and that
// channels are freed when sounds finish, so sounds keep coming.
func TestStepSound(t *testing.T) {
	wadPath := os.Getenv("DOOM_WAD")
	if wadPath == "" {
		wadPath = "doom1.wad"
	}
	wad, err := os.ReadFile(wadPath)
	if err != nil {
		t.Skip("doom1.wad not present")
	}
	SetVirtualFileSystem(MemFS{"doom1.wad": wad})
	StepInit(nil, []string{"-iwad", "doom1.wad", "-warp", "1", "1", "-skill", "3"})
	StepSoundEvents() // level start
	counts := map[string]int{}
	lastPistol := int32(0)
	for i := 0; i < 700; i++ {
		cmd := TicCmd{}
		if i%20 == 0 {
			cmd.Buttons = ButtonAttack
		}
		StepTic(cmd)
		for _, ev := range StepSoundEvents() {
			if ev.Tic != int32(i+1) {
				t.Fatalf("event tic %d, want %d: %+v", ev.Tic, i+1, ev)
			}
			counts[ev.Kind+":"+ev.Name]++
			if ev.Name == "pistol" {
				lastPistol = ev.Tic
			}
		}
	}
	t.Logf("sound events: %v", counts)
	// Every bullet fired must be heard, right up to the end. Before step
	// mode had a sound module, channels were never freed.
	fired := 50 - GetStats().Ammo
	if fired < 30 || counts["start:pistol"] != int(fired) || lastPistol < 680 {
		t.Fatalf("fired %d bullets but heard %d pistol shots (last at tic %d)", fired, counts["start:pistol"], lastPistol)
	}
}
