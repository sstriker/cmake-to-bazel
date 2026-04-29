cmake_minimum_required(VERSION 3.20)
project(lib-b LANGUAGES C VERSION 0.1.0)

add_library(lib-b STATIC lib-b.c)
target_include_directories(lib-b PUBLIC
    $<BUILD_INTERFACE:${CMAKE_CURRENT_SOURCE_DIR}/include>
    $<INSTALL_INTERFACE:include>)

install(TARGETS lib-b
        EXPORT libBTargets
        ARCHIVE DESTINATION lib)
install(DIRECTORY include/ DESTINATION include)
install(EXPORT libBTargets
        FILE libBTargets.cmake
        NAMESPACE lib_b::
        DESTINATION lib/cmake/lib-b)
