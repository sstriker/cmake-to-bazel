#!/usr/bin/env python3
"""Tiny codegen script driving the converter's add_custom_command recovery test.

Writes a version.h with a single VERSION_STRING macro to argv[1], using
argv[2] as the literal version string.
"""
import sys

dst, version = sys.argv[1], sys.argv[2]
with open(dst, "w") as f:
    f.write(f'#define VERSION_STRING "{version}"\n')
