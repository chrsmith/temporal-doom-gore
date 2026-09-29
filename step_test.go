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
