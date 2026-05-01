#include <stdio.h>
#include <zlib.h>
#include "foo.h"
int main(void) {
    /* zlib reference exercises the cross-element link path:
       -lz isn't produced in-trace, so it needs to resolve via
       the imports manifest's link_libraries lookup. */
    printf("foo + bar = %d (zlib %s)\n", foo() + bar(), zlibVersion());
    return 0;
}
