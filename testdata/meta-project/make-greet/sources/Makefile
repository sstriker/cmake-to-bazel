CC ?= cc
CFLAGS ?= -O2
DESTDIR ?=
prefix ?= /usr

all: greet

greet: greet.c
	$(CC) $(CFLAGS) -o $@ $<

install: greet
	install -D -m 0755 greet $(DESTDIR)$(prefix)/bin/greet

clean:
	rm -f greet

.PHONY: all install clean
