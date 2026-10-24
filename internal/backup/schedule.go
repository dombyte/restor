package backup

// jobState is where a job is in the schedule.
type jobState int

const (
	statePending jobState = iota
	stateRunning
	stateFinished
)

// schedule decides which jobs may start: a job starts once every job it runs after has
// finished (successfully or not), no running job holds one of its lock keys, and fewer
// than limit jobs run (0 = unlimited). Among the jobs that may start, the earlier one in
// the list starts first, so with limit 1 the jobs run in list order wherever the
// ordering allows. The before/after graph must be free of cycles (config checks it).
type schedule struct {
	jobs    []Job
	limit   int
	state   []jobState
	keys    [][]string
	index   map[string]int
	running int
}

func newSchedule(jobs []Job, limit int) *schedule {
	s := &schedule{
		jobs: jobs, limit: limit, state: make([]jobState, len(jobs)),
		keys: make([][]string, len(jobs)), index: make(map[string]int, len(jobs)),
	}
	for i, j := range jobs {
		s.keys[i] = j.Services.LockKeys()
		s.index[j.Settings.Name] = i
	}
	return s
}

// next marks the jobs that may start now as running and returns them.
func (s *schedule) next() []int {
	held := map[string]bool{}
	for i, st := range s.state {
		if st == stateRunning {
			s.hold(held, i)
		}
	}
	var started []int
	for i := range s.jobs {
		if s.limit > 0 && s.running >= s.limit {
			break
		}
		if !s.startable(held, i) {
			continue
		}
		s.state[i] = stateRunning
		s.running++
		s.hold(held, i)
		started = append(started, i)
	}
	return started
}

// startable reports whether job i is pending, its predecessors have finished and none of
// its lock keys is held.
func (s *schedule) startable(held map[string]bool, i int) bool {
	return s.state[i] == statePending && s.ready(i) && !s.conflicts(held, i)
}

// finish marks job i as finished.
func (s *schedule) finish(i int) {
	s.state[i] = stateFinished
	s.running--
}

// ready reports whether every job that i runs after has finished; jobs that are not in
// the schedule (not selected for this run) are ignored.
func (s *schedule) ready(i int) bool {
	for _, name := range s.jobs[i].Settings.After {
		if j, ok := s.index[name]; ok && s.state[j] != stateFinished {
			return false
		}
	}
	return true
}

func (s *schedule) conflicts(held map[string]bool, i int) bool {
	for _, k := range s.keys[i] {
		if held[k] {
			return true
		}
	}
	return false
}

func (s *schedule) hold(held map[string]bool, i int) {
	for _, k := range s.keys[i] {
		held[k] = true
	}
}
