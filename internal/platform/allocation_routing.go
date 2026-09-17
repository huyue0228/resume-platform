package platform

import "strings"

func isAllocationPath(path string) bool { return strings.HasPrefix(path, "position-pools/allocation-") }
