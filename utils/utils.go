package utils

import (
	"fmt"
	"os/exec"
	"syscall"

	"golang.org/x/term"
)

var (
	askedPass      bool
	cachedPassword string
)

func GetPassword() (string, error) {
	if askedPass {
		return cachedPassword, nil
	}

	fmt.Print("Enter Password: ")

	// syscall.Stdin is the file descriptor for standard input
	// ReadPassword disables terminal echo automatically
	bytePassword, err := term.ReadPassword(int(syscall.Stdin))
	if err != nil {
		return "", err
	}

	fmt.Println() // Print a newline because ReadPassword doesn't
	cachedPassword = string(bytePassword)
	askedPass = true
	return string(bytePassword), nil
}

func PrintHeader(header string) {
	fmt.Println("\n\n-----------------------------------------------------")
	fmt.Println(header)
	fmt.Println("-----------------------------------------------------")
}

func CheckCliCmdExist(cmd string) bool {
	_, err := exec.LookPath(cmd)
	return err == nil
}
