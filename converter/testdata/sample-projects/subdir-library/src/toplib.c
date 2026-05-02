#include "toplib.h"
#include "util.h"

const char *toplib_message(void) {
    static char buf[32];
    util_add(0, 0);
    return "toplib";
}
