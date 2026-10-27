package gore

import (
	"os"
	"testing"
)

// TestArchiveSpecialsWithRemovedThinker saves while a removed thinker is
// still in the thinker list. p_RemoveThinker only clears the function; the
// thinker is unlinked by the next p_RunThinkers, so this happens whenever a
// game is saved after something was removed during the previous tic.
func TestArchiveSpecialsWithRemovedThinker(t *testing.T) {
	savedCap, savedCeilings := thinkercap, activeceilings
	defer func() { thinkercap, activeceilings = savedCap, savedCeilings }()
	p_InitThinkers()
	activeceilings = [MAXCEILINGS]*ceiling_t{}

	var th thinker_t
	p_AddThinker(&th)
	p_RemoveThinker(&th)

	f, err := os.Create(t.TempDir() + "/save")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	save_stream = f
	defer func() { save_stream = nil }()
	p_ArchiveSpecials()
	if savegame_error != 0 {
		t.Fatal("error writing savegame")
	}
}
