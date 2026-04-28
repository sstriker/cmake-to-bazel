cmake_minimum_required(VERSION 3.20)
project(L41 LANGUAGES C VERSION 0.1.0)

find_package(L21 CONFIG REQUIRED)
find_package(L24 CONFIG REQUIRED)

add_library(L41 STATIC src/L41.c)
target_include_directories(L41 PUBLIC
    $<BUILD_INTERFACE:${CMAKE_CURRENT_SOURCE_DIR}/include>
    $<INSTALL_INTERFACE:include>)
target_link_libraries(L41 PUBLIC L21::L21 L24::L24)

install(TARGETS L41
        EXPORT L41Targets
        ARCHIVE DESTINATION lib)
install(EXPORT L41Targets
        FILE L41Targets.cmake
        NAMESPACE L41::
        DESTINATION lib/cmake/L41)
