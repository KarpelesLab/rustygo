package main

func main() {
	println("about to panic")
	msg := "multi\nline"
	panic(msg + " message")
}
