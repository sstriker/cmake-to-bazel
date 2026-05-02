#include <stdio.h>
int hot_compute(void);
int regular_value(void);
int main(void) {
    printf("optflags %d %d\n", hot_compute(), regular_value());
    return 0;
}
