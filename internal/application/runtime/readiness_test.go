package runtime

import "testing"

func TestReadiness(t *testing.T) {
	state := NewReadiness()
	if state.Ready() {
		t.Fatal("new state is ready")
	}
	state.SetReady(true)
	if !state.Ready() {
		t.Fatal("state did not become ready")
	}
	state.SetReady(false)
	if state.Ready() {
		t.Fatal("state did not become not-ready")
	}
}
