package claim

type Task struct {
	ID        string
	RuntimeID string
	TierID    string
}

func Claimable(task Task, runtimeID string, tierRuntimes map[string][]string) bool {
	if runtimeID == "" {
		return false
	}
	if task.TierID == "" {
		return task.RuntimeID != "" && task.RuntimeID == runtimeID
	}
	for _, rt := range tierRuntimes[task.TierID] {
		if rt == runtimeID {
			return true
		}
	}
	return false
}
