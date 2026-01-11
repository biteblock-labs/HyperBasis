package strategy

import "sync"

type StateMachine struct {
	mu    sync.Mutex
	State State
}

func NewStateMachine() *StateMachine {
	return &StateMachine{State: StateIdle}
}

func (s *StateMachine) Apply(event Event) State {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.State = nextState(s.State, event)
	return s.State
}

func nextState(current State, event Event) State {
	switch current {
	case StateIdle:
		if event == EventEnter {
			return StateEnter
		}
	case StateEnter:
		if event == EventHedgeOK {
			return StateHedgeOK
		}
		if event == EventExit {
			return StateExit
		}
	case StateHedgeOK:
		if event == EventExit {
			return StateExit
		}
	case StateExit:
		if event == EventDone {
			return StateIdle
		}
	}
	return current
}
