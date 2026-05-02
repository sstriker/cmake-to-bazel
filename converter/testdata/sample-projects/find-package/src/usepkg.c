#include <zlib.h>
#include "usepkg.h"

int usepkg_run(void) {
    return zlibVersion()[0] != '\0';
}
