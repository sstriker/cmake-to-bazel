#include "cons.h"
#include "prod.h"

int cons_run(void) {
    return prod_value() + 1;
}
