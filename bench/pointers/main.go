// Pointer chasing: build a linked list, then walk it repeatedly.
package main

type Node struct {
	Val  int
	Next *Node
}

func main() {
	var head *Node
	for i := 0; i < 1_000_000; i++ {
		head = &Node{Val: i * 7 % 1013, Next: head}
	}
	total := 0
	for round := 0; round < 200; round++ {
		for n := head; n != nil; n = n.Next {
			total += n.Val ^ round
		}
	}
	println(total)
}
