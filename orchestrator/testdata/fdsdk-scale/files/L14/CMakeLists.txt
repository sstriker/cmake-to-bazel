cmake_minimum_required(VERSION 3.20)
project(L14 LANGUAGES C VERSION 0.1.0)

find_package(L04 CONFIG REQUIRED)
find_package(L05 CONFIG REQUIRED)

add_library(L14 STATIC src/L14.c)
target_include_directories(L14 PUBLIC
    $<BUILD_INTERFACE:${CMAKE_CURRENT_SOURCE_DIR}/include>
    $<INSTALL_INTERFACE:include>)
target_link_libraries(L14 PUBLIC L04::L04 L05::L05)

install(TARGETS L14
        EXPORT L14Targets
        ARCHIVE DESTINATION lib)
install(EXPORT L14Targets
        FILE L14Targets.cmake
        NAMESPACE L14::
        DESTINATION lib/cmake/L14)
