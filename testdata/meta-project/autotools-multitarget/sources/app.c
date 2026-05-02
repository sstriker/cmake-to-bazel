#include <stdio.h>
#include "appcore.h"
#include "mathlib.h"

int main(void) {
    printf("%s; sq(7)=%d cb(3)=%d\n",
           appcore_greeting(),
           mathlib_square(7),
           mathlib_cube(3));
    return 0;
}
