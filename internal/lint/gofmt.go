package lint

import "go/format"

func gofmt(content []byte) ([]byte, error) { return format.Source(content) }
