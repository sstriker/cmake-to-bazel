cmake_minimum_required(VERSION 3.20)
project(L26 LANGUAGES C VERSION 0.1.0)

find_package(L06 CONFIG REQUIRED)
find_package(L07 CONFIG REQUIRED)

add_library(L26 STATIC src/L26.c)
target_include_directories(L26 PUBLIC
    $<BUILD_INTERFACE:${CMAKE_CURRENT_SOURCE_DIR}/include>
    $<INSTALL_INTERFACE:include>)
target_link_libraries(L26 PUBLIC L06::L06 L07::L07)

install(TARGETS L26
        EXPORT L26Targets
        ARCHIVE DESTINATION lib)
install(EXPORT L26Targets
        FILE L26Targets.cmake
        NAMESPACE L26::
        DESTINATION lib/cmake/L26)
