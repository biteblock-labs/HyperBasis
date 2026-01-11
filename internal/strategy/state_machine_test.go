package strategy

import "testing"

func TestStateMachineTransitions(t *testing.T) {
	sm := NewStateMachine()
	if sm.State != StateIdle {
		t.Fatalf("expected %s, got %s", StateIdle, sm.State)
	}
	if sm.Apply(EventEnter) != StateEnter {
		t.Fatalf("expected %s, got %s", StateEnter, sm.State)
	}
	if sm.Apply(EventHedgeOK) != StateHedgeOK {
		t.Fatalf("expected %s, got %s", StateHedgeOK, sm.State)
	}
	if sm.Apply(EventExit) != StateExit {
		t.Fatalf("expected %s, got %s", StateExit, sm.State)
	}
	if sm.Apply(EventHedgeOK) != StateHedgeOK {
		t.Fatalf("expected %s, got %s", StateHedgeOK, sm.State)
	}
	if sm.Apply(EventExit) != StateExit {
		t.Fatalf("expected %s, got %s", StateExit, sm.State)
	}
	if sm.Apply(EventDone) != StateIdle {
		t.Fatalf("expected %s, got %s", StateIdle, sm.State)
	}
}

func TestStateMachineInvalidTransition(t *testing.T) {
	sm := NewStateMachine()
	if sm.Apply(EventHedgeOK) != StateIdle {
		t.Fatalf("invalid transition should not change state")
	}
}

func TestStateMachineSetState(t *testing.T) {
	sm := NewStateMachine()
	sm.SetState(StateHedgeOK)
	if sm.State != StateHedgeOK {
		t.Fatalf("expected %s, got %s", StateHedgeOK, sm.State)
	}
}
