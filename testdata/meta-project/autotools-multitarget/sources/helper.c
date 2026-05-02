#include <stdio.h>
#include "appcore.h"

/* Helper binary: small companion that lives in libexec, not bin.
   Built with extra warning flags (per-target CFLAGS override in
   the Makefile) to exercise the converter's target-specific
   flag handling. */
int main(void) {
    printf("[helper] %s\n", appcore_greeting());
    return 0;
}
