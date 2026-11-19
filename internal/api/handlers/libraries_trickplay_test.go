package handlers

type countingReconciler struct{ calls int }

func (c *countingReconciler) ReconcileSoon() { c.calls++ }
