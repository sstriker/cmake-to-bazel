multitarget — small fixture that exercises:
  - multiple compiled artifacts (libmathlib.a + libappcore.a)
  - multiple binaries with different install dests (bin/app + libexec/helper)
  - per-target CFLAGS overrides
  - install layout with bin/, libexec/, lib/, include/, share/
