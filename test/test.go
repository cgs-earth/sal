package test

import "fmt"

type TestCmd struct {
}

func (c *TestCmd) Run() error {
	return fmt.Errorf("sal test is currently not yet implemented")
}
