cmake_minimum_required(VERSION 3.20)
project(hello LANGUAGES C VERSION 0.1.0)

add_library(hello STATIC hello.c)
target_include_directories(hello PUBLIC
    $<BUILD_INTERFACE:${CMAKE_CURRENT_SOURCE_DIR}/include>
    $<INSTALL_INTERFACE:include>)

install(TARGETS hello
        EXPORT helloTargets
        ARCHIVE DESTINATION lib)
install(DIRECTORY include/ DESTINATION include)
install(EXPORT helloTargets
        FILE helloTargets.cmake
        NAMESPACE hello::
        DESTINATION lib/cmake/hello)
