#include "genex.h"

int genex_compute(int x) {
#ifdef RELEASE_BUILD
    return x * 2;
#else
    return x;
#endif
}
