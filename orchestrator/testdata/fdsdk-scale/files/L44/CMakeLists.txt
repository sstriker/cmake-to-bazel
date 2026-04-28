cmake_minimum_required(VERSION 3.20)
project(L44 LANGUAGES C VERSION 0.1.0)

find_package(L24 CONFIG REQUIRED)
find_package(L27 CONFIG REQUIRED)

add_library(L44 STATIC src/L44.c)
target_include_directories(L44 PUBLIC
    $<BUILD_INTERFACE:${CMAKE_CURRENT_SOURCE_DIR}/include>
    $<INSTALL_INTERFACE:include>)
target_link_libraries(L44 PUBLIC L24::L24 L27::L27)

install(TARGETS L44
        EXPORT L44Targets
        ARCHIVE DESTINATION lib)
install(EXPORT L44Targets
        FILE L44Targets.cmake
        NAMESPACE L44::
        DESTINATION lib/cmake/L44)
