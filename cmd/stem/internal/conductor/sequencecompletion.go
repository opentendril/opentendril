package conductor

func (r *sequenceRunner) completeStep(stepID string) error {
	step := r.stepByID[stepID]
	if step == nil {
		return nil
	}
	step.Status = sequenceStatusComplete
	r.completed++
	if err := SaveSequence(r.path, r.seq); err != nil {
		return err
	}
	for _, dependentID := range r.dependents[stepID] {
		r.remainingDeps[dependentID]--
		if r.remainingDeps[dependentID] <= 0 && r.stepByID[dependentID].Status != sequenceStatusComplete && !r.queued[dependentID] {
			r.ready = append(r.ready, dependentID)
			r.queued[dependentID] = true
		}
	}
	r.sortReady()
	return nil
}
