#include "visi.h"
#include "internal.h"

int visi_internal_helper(int x) {
    return x + 1;
}

int visi_public_api(int x) {
    return visi_internal_helper(x) * 2;
}
