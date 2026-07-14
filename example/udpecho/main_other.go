//go:build !linux

package main

import "fmt"

func main() { fmt.Println("udpecho: linux only") }
