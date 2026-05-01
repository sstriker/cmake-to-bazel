cmake_minimum_required(VERSION 3.20)
project(hello LANGUAGES C)
add_executable(hello hello.c)
install(TARGETS hello)
