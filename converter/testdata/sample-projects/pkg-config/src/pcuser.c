#include <zlib.h>
#include "pcuser.h"

int pcuser_run(void) {
    return zlibVersion()[0] != '\0';
}
