// Package checkout provides shared local git working-tree lifecycle helpers.
//
// A checkout is the local thing Thane owns: a working tree at a path, possibly
// backed by a remote and possibly governed by signing or verification policy.
// Domain packages such as document roots and forge subscriptions attach their
// own meaning to a checkout; this package keeps the local git mechanics in one
// place.
package checkout
