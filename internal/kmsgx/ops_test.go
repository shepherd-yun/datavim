package kmsgx

import "testing"

func TestConfirmFor(t *testing.T) {
	for _, tc := range []struct {
		op        Op
		dev, prod Confirm
		level     Level
	}{
		{OpProduce, ConfirmNone, ConfirmYes, LevelWrite},
		{OpCreateTopic, ConfirmYes, ConfirmYes, LevelWrite},
		{OpResetOffsets, ConfirmYes, ConfirmTypeName, LevelWrite},
		{OpDeleteTopic, ConfirmTypeName, ConfirmTypeName, LevelDanger},
	} {
		if got := ConfirmFor(tc.op, false); got != tc.dev {
			t.Errorf("ConfirmFor(%v, dev) = %v, want %v", tc.op, got, tc.dev)
		}
		if got := ConfirmFor(tc.op, true); got != tc.prod {
			t.Errorf("ConfirmFor(%v, prod) = %v, want %v", tc.op, got, tc.prod)
		}
		if got := LevelOf(tc.op); got != tc.level {
			t.Errorf("LevelOf(%v) = %v, want %v", tc.op, got, tc.level)
		}
	}
}
