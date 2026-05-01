#include <zlib.h>
#include "mfi.h"

int mfi_run(void) {
    return zlibVersion()[0] != '\0';
}
