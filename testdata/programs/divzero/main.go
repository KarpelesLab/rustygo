package main

func div(a, b int) int { return a / b }

func main() {
	println("before")
	println(div(1, 0))
	println("after")
}
