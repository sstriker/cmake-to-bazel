#include <stdio.h>
#include "foo.h"
int main(void) {
    printf("foo + bar = %d\n", foo() + bar());
    return 0;
}
