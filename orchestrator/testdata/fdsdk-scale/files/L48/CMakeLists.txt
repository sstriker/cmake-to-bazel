cmake_minimum_required(VERSION 3.20)
project(L48 LANGUAGES C VERSION 0.1.0)

find_package(L33 CONFIG REQUIRED)
find_package(L38 CONFIG REQUIRED)
find_package(L40 CONFIG REQUIRED)

add_library(L48 STATIC src/L48.c)
target_include_directories(L48 PUBLIC
    $<BUILD_INTERFACE:${CMAKE_CURRENT_SOURCE_DIR}/include>
    $<INSTALL_INTERFACE:include>)
target_link_libraries(L48 PUBLIC L33::L33 L38::L38 L40::L40)

install(TARGETS L48
        EXPORT L48Targets
        ARCHIVE DESTINATION lib)
install(EXPORT L48Targets
        FILE L48Targets.cmake
        NAMESPACE L48::
        DESTINATION lib/cmake/L48)
