cmake_minimum_required(VERSION 3.20)
project(L33 LANGUAGES C VERSION 0.1.0)

find_package(L13 CONFIG REQUIRED)
find_package(L16 CONFIG REQUIRED)

add_library(L33 STATIC src/L33.c)
target_include_directories(L33 PUBLIC
    $<BUILD_INTERFACE:${CMAKE_CURRENT_SOURCE_DIR}/include>
    $<INSTALL_INTERFACE:include>)
target_link_libraries(L33 PUBLIC L13::L13 L16::L16)

install(TARGETS L33
        EXPORT L33Targets
        ARCHIVE DESTINATION lib)
install(EXPORT L33Targets
        FILE L33Targets.cmake
        NAMESPACE L33::
        DESTINATION lib/cmake/L33)
