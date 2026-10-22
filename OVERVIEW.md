# TBound — current state 22.10

## Prompt i used

```prompt
/pstack:in-your-own-words explain where we stand with tbound implementation for a overview: i dont want to overengineer too much, so i need a simple understandable "big picture" whats still left todo, and what cn be done with current state
```

## Output

**What I think you're after**

You want to step back from the daily details and see TBound as a whole. Your worry is that it keeps growing more complex while the thesis still has no results, so you want three things:

- how close we are to having something that produces thesis evidence;
- what is actually still left, without the jargon;
- what the system can already do today, so you can work with it now if it makes sense.

The real problem is a trade-off: get the thesis claims tested soon enough, without building more machinery than they need.

**The big picture**

TBound's job for the thesis is to run a coding agent inside a locked box and prove two things: what the agent tried to do, and what actually changed. All the pieces of that box are built: the isolation, the mediated tools, the signed setup and the evidence recorders.

What's missing is one full, real run on the test VM that goes all the way through: read, edit, run a command, publish the change. Each attempt so far has found a bug that only shows up on the real machine. All of those are now fixed in F4, but F4 isn't frozen or tested on the VM yet.

**Still left to do**

1. **Freeze F4 and get one full run through (G1).** This is the gate; everything below waits on it. Expect a day or so, possibly one more fix if the VM finds another bug.
2. **Two short probe runs and collecting the last 4 evidence items.** About half a day.
3. **Your part with your advisor:** approve the test case set and the pre-registration. This runs in parallel.
4. **A small pilot run, then the real thesis run (H1).** That produces the actual research results.

That's it. Nothing new needs to be built for the thesis. The product ideas, such as easier setup or the provisioning tool, are explicitly parked until after the thesis.

**What you can do with the current state**

- **Offline (no VM):** run the system on your machine with the built-in test fixture, the evaluation tooling and the 18 evidence checks that already pass. Good for writing the thesis's method and system chapters, with real logs and receipts as examples.
- **On the VM:** a single governed read-only task already worked end to end (the smoke run on F1). That's a valid demo of the boundary and its evidence, just not the full G1 scenario.
- **Already concrete for writing:**
  - the threat model, the architecture and the evaluation design;
  - today's defects as engineering findings, e.g. "the real-host validation found N defects the tests missed";
  - the kernel limitation, as a stated limitation.

**Overengineering**

The heavy part is per-run signing and freezing, and that's where most of the pain came from. For the thesis, keep it: it's what makes the evidence trustworthy. But don't add anything new. The rule from here on: only fix what blocks G1, the probes, the pilot or H1.

Is that the picture you had in mind? Or do you want me to cut further, for example skip the two probe runs if your advisor agrees those evidence items aren't essential to the claims?