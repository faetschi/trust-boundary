// Package sessionlaunch composes a verified sessionrepo generation with a
// supervisor-private, freshly initialized Git snapshot before any worker view
// is exposed. It does not launch Pi, assert a production host profile, or claim
// D06 worker isolation by itself.
package sessionlaunch
