package scheduler

// PriorityInput is the set of signals feeding the weighted scheduling score
// (design doc §6.3). It is a plain struct so Priority stays a pure function.
type PriorityInput struct {
	UserPriority       int     // 0-10; 0 means "use default 5"
	SchedulerTier      int     // root=10, sub-scheduler=5, worker=1
	WaitTimeSeconds    int64   // time already queued (anti-starvation)
	ResourceEfficiency float64 // 0-1: how well the task uses this node's capacity
}

// Priority returns the weighted scheduling score (design doc §6.3), higher is
// more urgent:
//
//	0.3*user_priority + 0.2*scheduler_tier + 0.1*wait_time + 0.4*resource_efficiency
//
// wait_time contributes positively so a long-queued task rises over time
// (anti-starvation). The design doc's pseudocode wrote w3 = -wait_time, which
// would starve rather than help; this implementation follows the stated intent.
func Priority(in PriorityInput) float64 {
	up := float64(in.UserPriority)
	if up == 0 {
		up = 5 // default
	}
	return 0.3*up + 0.2*float64(in.SchedulerTier) +
		0.1*float64(in.WaitTimeSeconds) + 0.4*in.ResourceEfficiency
}
