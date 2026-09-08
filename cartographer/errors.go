package main

import "errors"

var errUnexpectedForm = errors.New("provider answered with a form that is not a scan")
