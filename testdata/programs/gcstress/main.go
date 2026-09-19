package main

// Allocation-heavy, with references that must survive collections: garbage
// in every shape the M1 collector has to trace (heap objects, interior
// pointers, strings in struct fields, aggregates held in locals).

type Node struct {
	Val  int
	Name string
	Next *Node
}

type Pair struct {
	A, B *Node
	Tag  string
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	s := ""
	for n > 0 {
		s = string(rune('0'+n%10)) + s
		n /= 10
	}
	return s
}

// build makes a chain of n nodes; only the head is kept by the caller.
func build(n int) *Node {
	var head *Node
	for i := 0; i < n; i++ {
		head = &Node{Val: i, Name: "node-" + itoa(i), Next: head}
	}
	return head
}

func sum(head *Node) (int, int) {
	total, names := 0, 0
	for n := head; n != nil; n = n.Next {
		total += n.Val
		names += len(n.Name)
	}
	return total, names
}

func main() {
	// A long-lived chain, kept across many collections.
	keep := build(500)
	keepSum, keepNames := sum(keep)

	// Garbage: each chain dies before the next is built.
	last := 0
	for round := 0; round < 200; round++ {
		garbage := build(200)
		s, _ := sum(garbage)
		last = s
	}
	println("garbage", last)

	// An aggregate held in a local across allocations: its strings and
	// pointers must stay reachable through the whole loop.
	p := Pair{A: keep, B: build(10), Tag: "pair-" + itoa(42)}
	for round := 0; round < 100; round++ {
		_ = build(100)
	}
	a, _ := sum(p.A)
	b, _ := sum(p.B)
	println(p.Tag, a, b, len(p.Tag))

	// An interior pointer into a heap object, live across collections.
	field := &keep.Next.Val
	*field = 1234
	for round := 0; round < 50; round++ {
		_ = build(100)
	}
	println("interior", *field, keep.Next.Val, keep.Next.Name)

	again, againNames := sum(keep)
	println("kept", keepSum, keepNames, again == keepSum+1234-498, againNames == keepNames)
}
