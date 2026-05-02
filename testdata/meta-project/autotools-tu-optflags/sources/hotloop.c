int hot_compute(void) {
    /* Tight loop the user wants always-optimized. The Makefile
       declares `hotloop.o: CFLAGS += -O2` so this TU compiles
       at -O2 even when the global CFLAGS is -O0 (e.g. a debug
       build of the project as a whole). The trace-driven
       converter preserves the per-target -O2 in copts via
       make-db awareness. */
    int s = 0;
    for (int i = 0; i < 1000; i++) s += i;
    return s;
}
