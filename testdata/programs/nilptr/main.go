package main

type T struct{ x int }

func get(p *T) int { return p.x }

func main() {
	println(get(&T{5}))
	var p *T
	println(get(p))
}
