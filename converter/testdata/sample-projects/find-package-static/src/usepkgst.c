#include <zlib.h>
#include "usepkgst.h"

int usepkgst_run(void) {
    return zlibVersion()[0] != '\0';
}
