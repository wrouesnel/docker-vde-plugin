/*
	This unit exists because libnetwork doesn't let us deterministically remove
	tap interfaces in sandboxes - they pop back into the root namespace sometime
	*after* leave is called.

	To handle this, we have a simple GC system which stores a map of names we
	want removed, and polls at 1 second intervals to see if they've re-appeared
	and if we can delete them.
 */

package main
